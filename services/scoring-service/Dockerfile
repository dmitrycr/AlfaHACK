FROM golang:1.25.1-alpine AS build
WORKDIR /src
COPY libs/go ./libs/go
COPY contracts ./contracts
COPY services/scoring-service ./services/scoring-service
WORKDIR /src/services/scoring-service
ENV GOWORK=off CGO_ENABLED=0
RUN go mod download
RUN go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/app

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 appuser
COPY --from=build /out/app /app
COPY services/scoring-service/config/scoring.json /etc/alfa/scoring.json
ENV SCORING_CONFIG=/etc/alfa/scoring.json
COPY THIRD_PARTY_NOTICES /usr/share/licenses/alfa-insight/THIRD_PARTY_NOTICES
COPY docs/licenses /usr/share/licenses/alfa-insight/dependencies
USER appuser
EXPOSE 8080 9090
HEALTHCHECK --interval=5s --timeout=3s --retries=20 CMD wget -q -O- http://127.0.0.1:8080/readyz || exit 1
ENTRYPOINT ["/app"]
