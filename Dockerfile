FROM golang:1.24.10 AS build

WORKDIR /workspace
ENV GO111MODULE=on
ENV GOTOOLCHAIN=local
ENV CGO_ENABLED=0
ENV GOPROXY=https://goproxy.cn,direct

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /out/nse-ovs-lb .

FROM alpine:3.20

RUN apk add --no-cache iproute2

COPY --from=build /out/nse-ovs-lb /bin/nse-ovs-lb
ENTRYPOINT ["/bin/nse-ovs-lb"]
