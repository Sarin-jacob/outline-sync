FROM ghcr.io/astral-sh/uv:python3.12-alpine AS builder
WORKDIR /app
ENV UV_COMPILE_BYTECODE=1
ENV UV_LINK_MODE=copy
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project

FROM python:3.12-alpine
RUN apk add --no-cache git openssh-client
WORKDIR /app
COPY --from=builder /app/.venv /app/.venv
COPY main.py .
ENV PATH="/app/.venv/bin:$PATH"
ENV PYTHONUNBUFFERED=1
VOLUME /app/repos
CMD ["python", "main.py"]