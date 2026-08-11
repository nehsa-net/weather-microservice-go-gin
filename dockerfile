# Multi-stage build.
#
# The original was a single stage on golang:latest, which shipped the whole Go
# toolchain and every source file in the runtime image — roughly 800MB, and it
# would have baked openweatherapi.key into the image had one been present.
FROM golang:1.24-alpine AS build
WORKDIR /src

# Copy the manifests first so `go mod download` is cached independently of the
# source: an edit to a .go file then does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary that runs on a distroless base.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/weather-microservice .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/weather-microservice /weather-microservice

ENV GIN_MODE=release
EXPOSE 8080
USER nonroot:nonroot

# The API key comes from the environment (OPENWEATHER_API_KEY) — never from a
# file inside the image.
ENTRYPOINT ["/weather-microservice"]
