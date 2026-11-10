import hashlib
import pathlib

root = pathlib.Path("/app/data")
root.mkdir(parents=True, exist_ok=True)
with open(root / "inventory.csv", "w") as handle:
    handle.write("id,sku,qty,bin\n")
    for number in range(1, 6001):
        sku = "SK-" + hashlib.md5(f"sku{number}".encode()).hexdigest()[:6].upper()
        handle.write(f"{number},{sku},{number % 50},B{number % 17:02d}\n")
