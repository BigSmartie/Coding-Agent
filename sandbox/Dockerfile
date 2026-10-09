# Build this image explicitly before enabling command or MCP execution.
# Add your trusted offline toolchain/dependency caches here during image build.
FROM golang:1.25-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends python3 && rm -rf /var/lib/apt/lists/* && command -v sh && command -v cp && mkdir -p /workspace && chmod 0755 /workspace
WORKDIR /workspace
USER 65534:65534
CMD ["/bin/sh"]
