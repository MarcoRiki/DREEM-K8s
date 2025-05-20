FROM python:3.11-slim

WORKDIR /app

# Copia i file nel container
COPY main.py .
COPY requirements.txt .

# Installa le dipendenze nel container
RUN pip install --no-cache-dir -r requirements.txt

CMD ["python", "main.py"]
