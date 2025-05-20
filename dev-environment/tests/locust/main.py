from locust import HttpUser, task, between, LoadTestShape
import math
import cv2
from io import BytesIO

image_path = "dog_bike_car.jpg"
frame = cv2.imread(image_path)
_, frame_encoded = cv2.imencode('.jpg', frame, [cv2.IMWRITE_JPEG_QUALITY, 10])
# frame_bytes = frame_encoded.tobytes()
frame_bytes = BytesIO(frame_encoded)

class MyUser(HttpUser):
    wait_time = between(1, 2)

    @task
    def index(self):
        self.client.post("/process_frames", files={'frames': ('frame.jpg', frame_bytes.getvalue(), 'image/jpeg')})
        #self.client.get("/nodes/scaleUp")
# class SinusoidalSteppedShape(LoadTestShape):
#     """
#     Curva sinusoidale, ma ogni valore è mantenuto fisso per 1 minuto.
#     Il numero di utenti cambia bruscamente ogni step, con scaling in 1 secondo.
#     """
#     min_users = 20
#     max_users = 100
#     cycle_duration = 60 * 60     #  minuti per una sinusoide completa
#     step_duration = 30           # secondi per step
#
#     def __init__(self):
#         super().__init__()
#         self.current_users = None
#         self.last_step_time = 0
#
#     def tick(self):
#         run_time = self.get_run_time()
#
#         # Se è tempo di aggiornare
#         if self.current_users is None or run_time - self.last_step_time >= self.step_duration:
#             cycle_fraction = run_time / self.cycle_duration
#             amplitude = (self.max_users - self.min_users) / 2
#             offset = self.min_users + amplitude
#
#             # Calcolo punto sulla sinusoide
#             next_users = int(offset + amplitude * math.sin(2 * math.pi * cycle_fraction))
#
#             # Calcola il numero di utenti da aggiungere o rimuovere
#             spawn_rate = abs(next_users - self.current_users) if self.current_users is not None else 1000
#
#             self.current_users = next_users
#             self.last_step_time = run_time
#
#             return (self.current_users, spawn_rate)
#
#         # Mantieni il numero corrente di utenti
#         return (self.current_users, 1)
class SinusoidalShape(LoadTestShape):
    """
    Carico a sinusoide:
    - base_users: numero medio di utenti
    - amplitude: ampiezza della sinusoide (max variazione)
    - period: durata del ciclo completo in secondi
    """
    base_users = 50
    amplitude = 35
    period = 3*60  # un'onda ogni 60 secondi
    spawn_rate = 2  # utenti al secondo

    def tick(self):
        run_time = self.get_run_time()

        # Calcolo utenti attuali con funzione seno
        user_count = self.base_users + self.amplitude * math.sin(2 * math.pi * run_time / self.period)

        # Assicura che non ci siano utenti negativi
        user_count = max(0, int(user_count))

        return (user_count, self.spawn_rate)