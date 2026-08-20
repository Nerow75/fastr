## What this changes

<!-- One or two sentences. Link the issue it closes: Closes #123 -->

## Why

<!-- The problem being solved. If a spec exists under specs/, link it. -->

## Constitution check

<!-- See .specify/memory/constitution.md. Tick what applies, explain anything you cannot tick. -->

- [ ] No outbound network access beyond the local network
- [ ] Nothing new required on the phone beyond a browser
- [ ] No hard-coded file size cap, no whole-file buffering
- [ ] Behaves identically on Linux and Windows
- [ ] No filesystem exposure beyond the receive folder and explicitly offered files
- [ ] No secret, certificate, personal path, or local IP committed

## Added complexity

<!-- Any new dependency, service, or abstraction layer, and why the simpler option was rejected.
     Write "None" if there is none. -->

## Testing

<!-- What you ran, and on which operating systems. -->

- [ ] Tested on Linux
- [ ] Tested on Windows
- [ ] Tested against a mobile browser (state which)
