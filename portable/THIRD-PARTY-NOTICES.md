# VulnHarvest v0.0.1 third-party notices

The root LICENSE is GNU AGPL version 3. The frontend retains its MIT license
(licenses/web-MIT.txt), and go-mitmproxy retains its upstream MIT license
(licenses/go-mitmproxy-MIT.txt). Copyright notices are preserved verbatim.

The embedded Norma SDK archive did not declare a LICENSE. No license is invented
for it here. Its scoped provenance is maintained in the source repository under
third_party/norma. Redistribution terms for that SDK remain unresolved and need
review before public distribution. This is a recorded limitation, not a claim of
permission.

Go module identities and existing ARTEX environment names remain compatible.
runtime/bin includes the matching VulnHarvest tool relay, built from the root
AGPL source using only the Go standard library. No optional external tools are
included. Operators supply
those tools and their respective licenses independently. The package includes
only the exact skill files listed in the source portable/skills-manifest.txt.
