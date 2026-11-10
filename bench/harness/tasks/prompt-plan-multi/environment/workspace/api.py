from users import fetch_user


def handle(user_id):
    return {"user": fetch_user(user_id)}
