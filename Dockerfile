FROM golang:1.23-alpine

WORKDIR /app

COPY go.mod go.sum ./
ENV GOPROXY=direct
RUN go mod download

RUN go build -o server main.go

EXPOSE 8080
CMD ["./server"]