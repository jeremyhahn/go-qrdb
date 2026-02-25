# Task Completion Checklist

When a coding task is completed, verify the following:

## Before Marking Complete
1. **Code compiles**: `make build` passes
2. **Unit tests pass**: `make test` or `make test-{package}` for affected packages
3. **Code coverage**: `make coverage-{package}` shows >= 91% for affected packages
4. **Formatting**: `make fmt` passes (all files properly formatted)
5. **Linting**: `make vet` passes
6. **Each function has at least 2 tests**: one happy path, one error/edge case

## Code Quality
- Typed errors only (no fmt.Errorf)
- Map-based dispatch where applicable
- Lock-free patterns preferred
- Clean abstraction layers maintained
- No stub implementations
- DRY, YAGNI, KISS followed

## Testing Requirements
- All functions: minimum 2 tests (success + error case)
- Unit tests: fast, in-memory, no host modification, no blocking
- Target: 91%+ coverage per package
- No skip guards or -short flags

## NEVER Do
- Run git commands
- Create superfluous .md files
- Use simplified/mock implementations without approval
- Ignore errors with `_`
