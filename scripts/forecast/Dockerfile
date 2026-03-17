FROM python:3.10-slim

WORKDIR /app

# Installa solo le dipendenze di sistema necessarie
RUN apt-get update && apt-get install -y --no-install-recommends \
    && rm -rf /var/lib/apt/lists/*

# Copia requirements e installa dipendenze CPU-only di PyTorch
COPY requirements.txt .

# Installa PyTorch CPU-only (molto più leggero) e altre dipendenze
RUN pip install --no-cache-dir \
    torch==2.2.0+cpu \
    -f https://download.pytorch.org/whl/torch_stable.html && \
    pip install --no-cache-dir -r requirements.txt && \
    # Rimuovi file non necessari per ridurre dimensioni
    find /usr/local/lib/python3.10/site-packages -name "*.pyc" -delete && \
    find /usr/local/lib/python3.10/site-packages -name "*.pyo" -delete && \
    find /usr/local/lib/python3.10/site-packages -name "__pycache__" -type d -exec rm -rf {} + 2>/dev/null || true

# Copia il codice applicativo
COPY lstm_cpu_model_full_att.pt .
COPY forecast.py .
COPY utils.py .
COPY main.py .

ENTRYPOINT ["python", "main.py"]
