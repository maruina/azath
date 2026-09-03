# Security policy

## Scope

This policy covers the azath server (`azath serve`), the client CLIs
(`azath client`, `azath seal`), and the Telegram approval gate. Deployment
topology, reverse-proxy configuration, and host hardening are out of scope.

## Reporting a vulnerability

Email [matteo.ruina@gmail.com](mailto:matteo.ruina@gmail.com) with:

- a description of the issue and its impact,
- steps to reproduce it, including the azath version and the shape of your
  configuration (redact tokens, key material, and sealed blobs),
- what you expected versus what happened.

GitHub's private vulnerability reporting is also enabled on this repository
and is the preferred channel if you want to coordinate a fix publicly.

Please do not open public issues for suspected vulnerabilities.
