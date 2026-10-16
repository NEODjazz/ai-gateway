# Runtime provenance

The base image is `ghcr.io/docling-project/docling-serve-cpu:v1.36.0`, pinned by
multi-platform digest in the Dockerfile. Docling and Docling Serve are upstream
MIT projects. Redistribution also includes their image's third-party packages
and model artifacts, whose individual license notices remain applicable; this
notice does not relicense those dependencies or claim every model is MIT.

Sources: https://github.com/docling-project/docling and
https://github.com/docling-project/docling-serve/tree/v1.36.0 .

The local API admission/request boundary and worker logging entry point are small
integration extensions. They do not replace the upstream document converter.
