# Maintained kdbgo fork

This directory is a scoped fork of
[`github.com/sv/kdbgo` v0.20.0](https://github.com/sv/kdbgo/tree/v0.20.0),
retaining its MIT license. It is maintained as an in-tree package at
`github.com/greg/asyncq/third_party/kdbgo`, rather than as a nested Go module,
so source archives and module zip files include it.

The fork is maintained by asyncQ for its kdb+ IPC client. Security patches
bound wire and expanded frame sizes, isolate declared frames, validate decoded
shapes and arithmetic, make decompression panic-free, and add context-aware
TCP/TLS/authentication and operation APIs. Public value types, constants,
constructors, encoding semantics, and legacy dial/call wrappers remain
source-compatible where practical.

As in the upstream decoder, wire type `SD` is exposed as an `XD` dictionary
with the `SORTED` attribute; the encoder and `K.Len` also accept raw `SD`
values and preserve their logical dictionary length.

Only the production files and focused offline tests needed by asyncQ are
vendored. Upstream examples and tests that launch an external q process are
intentionally excluded.
