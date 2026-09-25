ARG BASE_IMAGE=cluster-health-monitor:test-latest

FROM ${BASE_IMAGE} AS chm-base

FROM registry.k8s.io/e2e-test-images/busybox:1.29-4@sha256:2e0f836850e09b8b7cc937681d6194537a09fbd5f6b9e08f4d646a85128e8937 AS tools
COPY test/e2e/testdata/gpu-tools/ /out/
RUN cp /bin/busybox /out/busybox \
    && chmod 0755 /out/busybox /out/nodechecker /out/nvidia-smi /out/nvbandwidth /out/mpirun

FROM ${BASE_IMAGE}
COPY --from=chm-base /nodechecker /real-nodechecker
COPY --from=tools --chown=65532:65532 /out/busybox /fake-gpu-tools/busybox
COPY --from=tools --chown=65532:65532 /out/nodechecker /nodechecker
COPY --from=tools --chown=65532:65532 /out/nvidia-smi /usr/bin/nvidia-smi
COPY --from=tools --chown=65532:65532 /out/nvbandwidth /usr/local/bin/nvbandwidth
COPY --from=tools --chown=65532:65532 /out/mpirun /opt/openmpi/bin/mpirun
