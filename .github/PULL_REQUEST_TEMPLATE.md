<!--
The title becomes the commit on main: a Conventional Commits header whose release impact equals
the highest of the commits' (CONTRIBUTING.md).
-->

**What and why**



**Checklist**

- [ ] `go test ./...` passes, and so do the suites behind a variable that the change touches
- [ ] Golden files are regenerated with `PHAENON_UPDATE=1`, and their diff is read
- [ ] A language change links its change in `phaenonlang/design`
