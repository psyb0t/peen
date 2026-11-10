import users


def nightly():
    return [users.fetch_user(number) for number in (1, 2, 3)]
