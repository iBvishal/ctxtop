# Working on ctxtop

- Go, standard library only unless a dependency clearly pays for itself. `go vet ./...` and `go test ./...` must pass; CI runs them on macOS and Linux with `-race`.
- One package per runtime under `internal/` (`llamacpp`, `ollama`, `lmstudio`). Each parses recorded responses in its `testdata/`; tests never need a real server or model.
- Field names come from the runtime's source, not blog posts. Note the version you checked against in the test fixture or package comment.
- ctxtop only sends GET requests to localhost (and runs `lms` read-only), never keeps or prints prompt or output text, and has no telemetry. Keep it that way: decode only the fields you need.
- The JSON in `internal/snapshot` is a public interface. Adding a field is fine; renaming or removing one bumps `snapshot.Version`.
- Demo data only in fixtures, README and screenshots. No real paths, hostnames or prompts.
- Commits and PRs carry no AI attribution lines.
