# Metrology Assurance Suite

Metrology Assurance Suite is a Go HTTP service for bounded laboratory
measurement assurance work. It registers devices, opens verification cases,
records measurements, handles failed tolerance decisions, and seals traceable
evidence records.

## Run

```bash
go run ./cmd/metrology-service --addr 127.0.0.1:18121
```

The service listens on `127.0.0.1:18121` by default.

## Build And Verify

```bash
go build ./...
go run ./cmd/workflowcheck --workflow register-device
go run ./cmd/workflowcheck --workflow open-verification-case
go run ./cmd/workflowcheck --workflow record-measurement
go run ./cmd/workflowcheck --workflow reopen-verification-case
go run ./cmd/workflowcheck --workflow seal-verification
go run ./cmd/workflowcheck --workflow laboratory-isolation
```

The workflow runner starts a fresh in-process HTTP service and exercises the
public API. It is a production smoke command, not a unit test.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add the unit and integration test suite and replace
or extend these smoke checks with red/green verification tests.

## Laboratory scoping

The service is shared by independent laboratories. Every device, case,
measurement, seal, and audit event belongs to exactly one laboratory. Write
requests carry a `lab` field and read requests carry a `?lab=` query parameter;
omitting the laboratory means `lab-default`. Resources owned by another
laboratory return `not_found` on every read and write, and a rejected request
leaves no audit event or partial record.

`POST /devices/{id}/transfer` formally hands a device to another laboratory:
the device and its unfinished case move together, so new measurements,
reopens, and seals run under the receiving laboratory. Sealed cases and the
history already recorded stay with the original laboratory.

## API

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}?lab=`
- `POST /devices/{id}/transfer`
- `POST /cases`
- `GET /cases/{id}?lab=`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events?lab=`

## Layout

- `cmd/metrology-service`: executable HTTP server.
- `cmd/workflowcheck`: bounded workflow verification commands.
- `internal/httpapi`: route and JSON boundary.
- `internal/assurance`: workflow orchestration.
- `internal/domain`: state rules and invariants.
- `internal/repository`: copy-on-write in-memory persistence.
- `internal/contracts`: API request and response models.
- `internal/bootstrap`: concrete dependency wiring.

## Inputs And Outputs

Inputs are JSON HTTP requests. Outputs are JSON response envelopes with either a
resource view or a stable error object:

```json
{
  "error": {
    "code": "state_conflict",
    "message": "case is not awaiting measurement"
  }
}
```

The service has no environment variables and no external dependencies.
