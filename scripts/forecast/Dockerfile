FROM python:3.10-slim

WORKDIR /app

# Copia i file nel container
COPY main.py .
COPY forecast.py .
COPY utils.py .
COPY cpu_forecast_model.h5 .
COPY requirements.txt .

# Installa le dipendenze nel container
RUN pip install --no-cache-dir -r requirements.txt

CMD ["python", "main.py"]
