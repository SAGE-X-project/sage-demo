# Commit and pull request rules

- Use Conventional Commits: a concise English `type: description` describing the actual behavior.
- Write all commit messages and pull request titles and descriptions in English.
- Never include emojis or co-author attribution, including Co-authored-by trailers, in human/agent-authored commits and pull requests. Dependabot-authored changes are the sole exception.
- Keep the content focused on the essential change. Describe behavior explicitly; do not use development phase names or task identifiers.
- Apply these rules to squash commit titles and bodies as well.
- Merge pull requests only with squash merge. After merging, delete the work branch locally and remotely, then update local main to match remote main.

# Pull request review requests

- Never request a code review from SAGE Core Team (`SAGE-X-project/sage-core-dev`), manually or automatically, for any pull request.
- Check CODEOWNERS, Dependabot and workflow assignments before PR creation or readiness. Do not add this team as a reviewer or send it review requests.

# Demo scope

- The demo compares one workload with and without SAGE protection. Faults are injected only into the demo's own processes on loopback; never write code that targets other systems, real services or deployed hosts, and never add host-bypass code.
- Implement and run scenario-based unit tests and bounded runtime tests for changed behavior, with real cryptographic exchanges.
- Report measured behavior only. A demo result is not a conformance verdict or a universal security claim; synthetic Registry Sources, accept-all measurements and account separation on one kernel must be stated as such.
