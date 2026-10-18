FROM golang:1.26.5 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go vet -tags=docker_e2e ./tests/docker
RUN CGO_ENABLED=0 go build -o /out/azath ./cmd/azath && CGO_ENABLED=0 go test -c -tags=docker_e2e -o /out/docker-e2e ./tests/docker

FROM caddy:2.10.2-alpine
COPY --from=build /out/azath /usr/local/bin/azath
COPY --from=build /out/docker-e2e /test/docker-e2e
RUN ln -s /test/docker-e2e /test/mount
ENTRYPOINT ["/test/docker-e2e", "-test.v", "-test.timeout=120s"]
