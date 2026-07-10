FROM golang:1.26-alpine AS build
WORKDIR /src
COPY main.go .
# The mock is dependency-free; init a throwaway module so `go build` works
# without copying smelt's go.mod (which would drag the whole module in).
RUN go mod init plcmock && CGO_ENABLED=0 go build -o /plcmock .

FROM alpine:3.21
COPY --from=build /plcmock /usr/bin/plcmock
EXPOSE 80
ENTRYPOINT ["/usr/bin/plcmock"]
