FROM golang:1.22-alpine AS base
WORKDIR /src
COPY . .
RUN go mod tidy

FROM base AS test
CMD ["sh","-c","go test ./... -covermode=atomic -coverpkg=./internal/... -coverprofile=/out/cover.out && go tool cover -func=/out/cover.out | tail -n 1 && go tool cover -html=/out/cover.out -o /out/cover.html"]

FROM base AS build
RUN CGO_ENABLED=0 go build -o /bin/server ./cmd/server

FROM alpine:3.20
COPY --from=build /bin/server /server
EXPOSE 8080
CMD ["/server"]
