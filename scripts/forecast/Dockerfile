FROM python:3.10-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copia il codice applicativo
COPY utils.py .
COPY pending.py .
COPY menouer.py .
COPY main.py .

ENTRYPOINT ["python", "main.py"]
