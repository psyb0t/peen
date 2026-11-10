"""User lookup helpers."""

USERS = {1: "ada", 2: "grace", 3: "linus"}


def fetch_user(user_id):
    return USERS.get(user_id, "unknown")
