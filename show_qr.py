import qrcode
import sys
import json
import requests

# Obter o ID do dispositivo da linha de comando ou usar o padrão 1
device_id = sys.argv[1] if len(sys.argv) > 1 else "1"

# Obter QR code da API
response = requests.get(f"http://localhost:8080/api/devices/{device_id}/qrcode")
data = response.json()
qr_code = data.get("qr_code")

if not qr_code:
    print("Não foi possível obter o QR code. Verifique se o dispositivo está aprovado.")
    sys.exit(1)

# Gerar imagem do QR code
qr = qrcode.QRCode(
    version=1,
    error_correction=qrcode.constants.ERROR_CORRECT_L,
    box_size=10,
    border=4,
)
qr.add_data(qr_code)
qr.make(fit=True)

img = qr.make_image(fill_color="black", back_color="white")
img.save("qrcode.png")
img.show()  # Mostrar a imagem
print("QR Code salvo como 'qrcode.png'")