"""A known live endpoint outside the guest's declared policy, inside test netns."""
import socket

with socket.socket() as server:
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind(("1.1.1.1", 443))
    server.listen(64)
    while True:
        connection, _ = server.accept()
        with connection:
            connection.sendall(b"synthetic-public-endpoint\n")
