When finished with a task:
Document changes in changes/yyyy-mm-dd-hh-mm-title.md
Update design decisions in DESIGN.md
Update architecture in ARCHITECTURE.md
Update the CLI help text
Update README.md

Each doc has a distinct scope — avoid duplicating content across them:
- **README.md**: User-facing — what it does, how to install, how to use
- **ARCHITECTURE.md**: Developer-facing — code structure, pipeline, memory model, extension points
- **DESIGN.md**: Rationale — why decisions were made, tradeoffs, bug fixes
