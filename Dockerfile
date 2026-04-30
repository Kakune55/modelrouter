FROM golang:1.26-trixie AS build

WORKDIR /src

ARG TARGETOS=linux
ARG TARGETARCH=amd64

COPY go.mod ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/modelrouter ./cmd/modelrouter


FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=build /out/modelrouter /usr/local/bin/modelrouter
COPY config.example.json /app/config.json

RUN addgroup -S appgroup && adduser -S appuser -G appgroup \
	&& mkdir -p /app/usage_logs \
	&& chown -R appuser:appgroup /app

USER appuser

EXPOSE 8080
VOLUME ["/app/usage_logs"]

ENTRYPOINT ["/usr/local/bin/modelrouter"]
CMD ["-addr", ":8080", "-config", "/app/config.json"]
