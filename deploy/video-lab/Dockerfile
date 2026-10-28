# Build the static Linux/amd64 binary first; no package installation is needed.
FROM mansiflix/video-worker:demo-88bb1d91e247
COPY video-lab /usr/local/bin/video-lab
USER 10001:10001
ENV WORK_DIR=/work
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/video-lab"]
