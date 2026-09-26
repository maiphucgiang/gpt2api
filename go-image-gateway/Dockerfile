FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /image-gateway .

FROM alpine:3.21
RUN adduser -D -H -u 10001 gateway
USER gateway
COPY --from=build /image-gateway /image-gateway
EXPOSE 8080
ENTRYPOINT ["/image-gateway"]
