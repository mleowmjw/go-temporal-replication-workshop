# AGENTS

## Language

- Use latest Go v1.26.x and its full capabilities
- Always prefer stdlib if available and it make sense

## Orchestration

- Use Temporal + Go SDK to handle the workflows
- Use Temporal testsuite as much as possible to have widest test coverage
- Use advanced techique like registerdelayedcallback to simulate long running system
- Ensure use Temporal Worker version to ensure multiple workflow versions can co-exists

## Runtime

- Whole system should be fully testable standalone with Go binary
- Use modern Go capabilities (when needed): generics, structured log, built-in http
routing, testing/synctest
- Use techniques like first class anonymous function as method replacement, synctest
 to ensure all things are deterministic

## Testing

- All new cases should be at least 80% coverage
- Unit tests and inetgration tests MUST be comepleted without needing to spin up any
 external dependencies
- Only Full End-to-End should need to stand up Temporal Test Server

## Tools

- Use mise to run tasks, set env variables, automate
- Setup env like CGO_ENABLED=0, PATH append to use ~/go/bin and loading from .env
- Tools available: ripgrep, fzf, air, goreleaser, watchexec
- Use overmind to start temporal-cli + air

## Specification (MVP)

- Follow cmd/replication-platform/PRD.md for suggested details but it MUST NOT override what stated here
- This is a workshop with 3 parts; start with cmd/session-1
- Once successful, then do cmd/session-2
- Finally complete cmd/session-3
- Ask if anything unsure or contradictory
- Use mise to launch the workshops for all sessions

## Specification (Advanced) 
- Finally CI/CD will use End-to-End Tests
- Implement this ONLY after Unit/Integration tests are passing

## Implementation Status & Learnings

<TODO>
