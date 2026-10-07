package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":          checkRegisterDevice,
		"open-verification-case":   checkOpenVerificationCase,
		"record-measurement":       checkRecordMeasurement,
		"reopen-verification-case": checkReopenVerificationCase,
		"seal-verification":        checkSealVerification,
		"laboratory-isolation":     checkLaboratoryIsolation,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Reviewer-%d", index+1),
				"summary":  "accepted measurement sealed for review",
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	return nil
}

func checkLaboratoryIsolation() error {
	h := newHarness()
	defer h.close()

	const lab1 = "lab-one"
	const lab2 = "lab-two"

	// Lab one registers a device and opens an in-progress case.
	deviceID, err := h.createDeviceLab("MET-2001", lab1)
	if err != nil {
		return err
	}
	opened, err := h.openCaseLab(deviceID, lab1)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}

	// 1. A foreign laboratory must not see the device or the case.
	foreignDevice, err := h.request(http.MethodGet, "/devices/"+deviceID+"?lab="+lab2, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(foreignDevice, http.StatusNotFound); err != nil {
		return err
	}
	foreignCase, err := h.request(http.MethodGet, "/cases/"+caseID+"?lab="+lab2, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(foreignCase, http.StatusNotFound); err != nil {
		return err
	}
	foreignEvents, err := h.request(http.MethodGet, "/cases/"+caseID+"/events?lab="+lab2, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(foreignEvents, http.StatusNotFound); err != nil {
		return err
	}

	// 2. A foreign laboratory cannot attach a measurement. The rejection must
	// leave no audit event and no measurement behind.
	intrusion, err := h.recordMeasurementLab(caseID, 25.2, "Outsider", lab2)
	if err != nil {
		return err
	}
	if err := expectStatus(intrusion, http.StatusNotFound); err != nil {
		return err
	}
	ownerView, err := h.request(http.MethodGet, "/cases/"+caseID+"?lab="+lab1, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(ownerView, http.StatusOK); err != nil {
		return err
	}
	measurements, err := nestedSlice(ownerView.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 0 {
		return fmt.Errorf("rejected foreign measurement left %d measurement(s)", len(measurements))
	}
	events, err := nestedSlice(ownerView.Body, "events")
	if err != nil {
		return err
	}
	for _, raw := range events {
		event, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == "measurement_requested" {
			return errors.New("rejected foreign measurement left a phantom audit event")
		}
	}

	// Reopen and seal must be denied to a foreign laboratory as well.
	foreignReopen, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Outsider",
		"reason": "trying to take over",
		"lab":    lab2,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(foreignReopen, http.StatusNotFound); err != nil {
		return err
	}
	foreignSeal, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Outsider",
		"summary":  "trying to seal",
		"lab":      lab2,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(foreignSeal, http.StatusNotFound); err != nil {
		return err
	}

	// 3. Lab one formally transfers the device to lab two. The open case must
	// move with the device; the original lab loses access to both.
	transfer, err := h.request(http.MethodPost, "/devices/"+deviceID+"/transfer", map[string]any{
		"lab":        lab1,
		"target_lab": lab2,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(transfer, http.StatusOK); err != nil {
		return err
	}
	owner, err := nestedString(transfer.Body, "device", "lab")
	if err != nil {
		return err
	}
	if owner != lab2 {
		return fmt.Errorf("device lab after transfer = %q, expected %q", owner, lab2)
	}
	oldLabDevice, err := h.request(http.MethodGet, "/devices/"+deviceID+"?lab="+lab1, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(oldLabDevice, http.StatusNotFound); err != nil {
		return err
	}
	movedView, err := h.request(http.MethodGet, "/cases/"+caseID+"?lab="+lab2, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(movedView, http.StatusOK); err != nil {
		return err
	}
	movedLab, err := nestedString(movedView.Body, "case", "lab")
	if err != nil {
		return err
	}
	if movedLab != lab2 {
		return fmt.Errorf("open case lab after transfer = %q, expected %q", movedLab, lab2)
	}
	oldLabCase, err := h.request(http.MethodGet, "/cases/"+caseID+"?lab="+lab1, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(oldLabCase, http.StatusNotFound); err != nil {
		return err
	}

	// 4. New business on the transferred case runs under lab two.
	recorded, err := h.recordMeasurementLab(caseID, 25.2, "New Tech", lab2)
	if err != nil {
		return err
	}
	if err := expectStatus(recorded, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(recorded.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("measurement after transfer state = %q", state)
	}
	sealed, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "New Reviewer",
		"summary":  "sealed after transfer",
		"lab":      lab2,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(sealed, http.StatusCreated); err != nil {
		return err
	}
	sealLab, err := nestedString(sealed.Body, "seal", "lab")
	if err != nil {
		return err
	}
	if sealLab != lab2 {
		return fmt.Errorf("seal lab = %q, expected %q", sealLab, lab2)
	}

	// 5. Historical facts keep their original laboratory: the device is now
	// lab two, but a sealed case produced in the original lab stays there.
	historyDeviceID, err := h.createDeviceLab("MET-2002", lab1)
	if err != nil {
		return err
	}
	historyOpened, err := h.openCaseLab(historyDeviceID, lab1)
	if err != nil {
		return err
	}
	historyCaseID, err := nestedString(historyOpened.Body, "case", "id")
	if err != nil {
		return err
	}
	historyRecorded, err := h.recordMeasurementLab(historyCaseID, 25.1, "Old Tech", lab1)
	if err != nil {
		return err
	}
	if err := expectStatus(historyRecorded, http.StatusCreated); err != nil {
		return err
	}
	historySealed, err := h.request(http.MethodPost, "/cases/"+historyCaseID+"/seal", map[string]any{
		"reviewer": "Old Reviewer",
		"summary":  "sealed before transfer",
		"lab":      lab1,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(historySealed, http.StatusCreated); err != nil {
		return err
	}
	if _, err := h.request(http.MethodPost, "/devices/"+historyDeviceID+"/transfer", map[string]any{
		"lab":        lab1,
		"target_lab": lab2,
	}); err != nil {
		return err
	}
	// Device moved to lab two, but the sealed history remains readable by and
	// owned by lab one.
	stayedView, err := h.request(http.MethodGet, "/cases/"+historyCaseID+"?lab="+lab1, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(stayedView, http.StatusOK); err != nil {
		return err
	}
	stayedLab, err := nestedString(stayedView.Body, "case", "lab")
	if err != nil {
		return err
	}
	if stayedLab != lab1 {
		return fmt.Errorf("sealed historical case lab = %q, expected %q", stayedLab, lab1)
	}
	stayedSealLab, err := nestedString(stayedView.Body, "seal", "lab")
	if err != nil {
		return err
	}
	if stayedSealLab != lab1 {
		return fmt.Errorf("historical seal lab = %q, expected %q", stayedSealLab, lab1)
	}
	movedAwayCase, err := h.request(http.MethodGet, "/cases/"+historyCaseID+"?lab="+lab2, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(movedAwayCase, http.StatusNotFound); err != nil {
		return err
	}
	return nil
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	return h.createDeviceLab(tag, "")
}

func (h *harness) createDeviceLab(tag, lab string) (string, error) {
	body := map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	}
	if lab != "" {
		body["lab"] = lab
	}
	response, err := h.request(http.MethodPost, "/devices", body)
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	return h.openCaseLab(deviceID, "")
}

func (h *harness) openCaseLab(deviceID, lab string) (apiResponse, error) {
	body := map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	}
	if lab != "" {
		body["lab"] = lab
	}
	response, err := h.request(http.MethodPost, "/cases", body)
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.recordMeasurementLab(caseID, observed, technician, "")
}

func (h *harness) recordMeasurementLab(caseID string, observed float64, technician, lab string) (apiResponse, error) {
	body := map[string]any{
		"observed":   observed,
		"technician": technician,
	}
	if lab != "" {
		body["lab"] = lab
	}
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", body)
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedSlice(root map[string]any, key string) ([]any, error) {
	value, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	return items, nil
}

func hasEvent(root map[string]any, kind string) bool {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return false
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return true
		}
	}
	return false
}

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
