FROM node:24-alpine AS frontend

WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.26.2-alpine AS backend

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY migrations/ ./migrations/
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/print-cam .

FROM alpine:3.22

RUN adduser -D -H -u 10001 appuser
WORKDIR /app
COPY --from=backend /out/print-cam /app/print-cam
COPY --from=frontend /src/frontend/dist /app/frontend/dist

ENV ADDR=:8080
ENV FRONTEND_DIST_DIR=/app/frontend/dist

EXPOSE 8080
USER appuser
ENTRYPOINT ["/app/print-cam"]
