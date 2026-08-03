# Wheel-only build: nothing here compiles, which keeps this usable on a small
# single-core box.
FROM python:3.13-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

WORKDIR /app

COPY requirements.txt .
RUN pip install --only-binary :all: -r requirements.txt

COPY app/ /app/app/

# Drop privileges. This container writes nothing to disk except logs to stdout.
RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin collector
USER 10001

EXPOSE 8000
CMD ["python", "-m", "app.main"]
