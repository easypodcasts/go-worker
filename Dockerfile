FROM alpine
RUN apk --update --no-cache add ca-certificates ffmpeg
COPY go-worker /usr/bin/go-worker
ENTRYPOINT ["/usr/bin/go-worker"]