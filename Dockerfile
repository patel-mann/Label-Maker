# build stage
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/labelprint .

# run stage
FROM alpine:3.21
RUN adduser -D -u 10001 app
USER app
COPY --from=build /out/labelprint /labelprint
EXPOSE 8080
ENTRYPOINT ["/labelprint"]
