# go-qrdb Code Style and Conventions

## Code Style
- Follow the official Google Go style guide: https://google.github.io/styleguide/go/guide
- Priorities: Clarity > Simplicity > Concision > Maintainability > Consistency

## Error Handling
- **Typed errors ONLY** - never use fmt.Errorf or inline errors
- Error types are structs with Error() method, wrappable errors include Unwrap()
- Error message prefix pattern: "package: description: " + details
- Example: `KeyNotFoundError{Key: k}` → "service: key not found: mykey"
- NEVER ignore errors with `_`

## Patterns
- **Map-based dispatch** over switch statements for O(1) lookup
- **Functional options** for configuring servers/components
- **Lock-free preferred** - use atomic package, not unsafe pointers
- **Prefer lock-free algorithms to mutexes**
- DRY, YAGNI, KISS principles
- Service layer as critical abstraction between API and business logic

## Naming
- Standard Go conventions (camelCase exports, unexported lowercase)
- Interface names: descriptive (KVService, AdminService, Host)
- Constructor pattern: NewXxx(deps...) returning interface type
- Private impl structs: lowercase (kvService, adminService)

## File Organization
- Each package has an `errors.go` for typed errors
- Test files: `*_test.go` in the same package
- Proto files: `proto/qrdbv1/` subdirectory under grpc package

## Documentation
- Apache 2.0 license header on all files
- GoDoc comments on exported types and functions
- Docs in `docs/` folder, kept concise
- Performance docs in `test/performance/` ONLY

## What NOT To Do
- Never use stubs, mocks, or simplified implementations without explicit approval
- Never create superfluous .md files
- Never use skip guards or -short flags in tests
- Never run git commands in this repository
