#define _DARWIN_C_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <signal.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/wait.h>
#include <termios.h>
#include <time.h>
#include <unistd.h>
#include <util.h>

enum { WRITE = 1, RESIZE, SIGNAL, EOF_INPUT, CLOSE };
enum { OPENED = 16, PTY, STDOUT, STDERR, ECHO_STATE, ACK, EXITED, EXEC_FAILED };
enum { APPLIED, AUTH_PAUSED, INPUT_CLOSED, UNSUPPORTED, IO_FAILED };
enum { FRAME_HEADER = 5, MAX_INPUT = 16384, OUTPUT_CHUNK = 8192 };

struct session {
    pid_t child;
    int input;
    int output;
    int error;
    bool pty;
    bool eof;
    int echo_state;
};

static volatile sig_atomic_t stopping;

static void stop(int sig) {
    (void)sig;
    stopping = 1;
}

static int64_t millis(void) {
    struct timespec now;
    clock_gettime(CLOCK_MONOTONIC, &now);
    return (int64_t)now.tv_sec * 1000 + now.tv_nsec / 1000000;
}

static void close_fd(int *fd) {
    if (*fd >= 0) close(*fd);
    *fd = -1;
}

static int fd_flags(int fd) {
    if (fcntl(fd, F_SETFD, FD_CLOEXEC) < 0) return -1;
    return fcntl(fd, F_SETFL, O_NONBLOCK);
}

static bool owner_lost(short events) {
    return (events & (POLLHUP | POLLERR | POLLNVAL)) != 0;
}

static uint32_t read_u32(const unsigned char *data) {
    return (uint32_t)data[0] << 24 | (uint32_t)data[1] << 16 | (uint32_t)data[2] << 8 | data[3];
}

static bool transfer(int fd, const unsigned char *data, size_t used, int64_t deadline) {
    while (used) {
        ssize_t count = write(fd, data, used);
        if (count > 0) {
            data += count;
            used -= (size_t)count;
            continue;
        }
        if (count == 0 || (errno != EINTR && errno != EAGAIN && errno != EWOULDBLOCK)) return false;
        int64_t left = deadline - millis();
        if (left <= 0 || stopping) return false;
        struct pollfd fds[] = {{STDIN_FILENO, 0, 0}, {fd, POLLOUT, 0}};
        if (poll(fds, 2, (int)left) < 0 && errno != EINTR) return false;
        if (owner_lost(fds[0].revents) || owner_lost(fds[1].revents)) return false;
    }
    return true;
}

static bool event(unsigned char type, const void *bytes, size_t size, int64_t deadline) {
    unsigned char header[] = {type, (unsigned char)(size >> 24), (unsigned char)(size >> 16),
        (unsigned char)(size >> 8), (unsigned char)size};
    return transfer(STDOUT_FILENO, header, sizeof(header), deadline) && transfer(STDOUT_FILENO, bytes, size, deadline);
}

static void exec_error(int fd) {
    int failure = errno;
    while (write(fd, &failure, sizeof(failure)) < 0 && errno == EINTR) {}
    _exit(127);
}

static int child_pipe(int fds[2]) {
    if (pipe(fds) < 0) return -1;
    if (fcntl(fds[0], F_SETFD, FD_CLOEXEC) < 0 || fcntl(fds[1], F_SETFD, FD_CLOEXEC) < 0) return -1;
    return 0;
}

static int exec_status(int fd) {
    const int64_t deadline = millis() + 1000;
    bool setup = false;
    int64_t remaining;
    while (!stopping && (remaining = deadline - millis()) > 0) {
        struct pollfd fds[] = {{STDIN_FILENO, 0, 0}, {fd, POLLIN, 0}};
        int result = poll(fds, 2, (int)remaining);
        if (result < 0 && errno == EINTR) continue;
        if (result <= 0 || stopping || owner_lost(fds[0].revents)) break;
        int status;
        ssize_t count = read(fd, &status, sizeof(status));
        if (count == 0 && setup) return 0;
        if (count != sizeof(status)) break;
        if (status != 0 || setup) return status ? status : EIO;
        setup = true;
    }
    return EIO;
}

static int launch(struct session *session, char **command, struct winsize *size) {
    int ready[2], input[2], output[2], error[2];
    if (child_pipe(ready) < 0 || (!session->pty && (child_pipe(input) < 0 || child_pipe(output) < 0 || child_pipe(error) < 0))) return -1;
    int master = -1;
    session->child = session->pty ? forkpty(&master, NULL, NULL, size) : fork();
    if (session->child < 0) return -1;
    if (session->child == 0) {
        close(ready[0]);
        signal(SIGTERM, SIG_DFL);
        signal(SIGHUP, SIG_DFL);
        signal(SIGPIPE, SIG_DFL);
        if (!session->pty) {
            if (setpgid(0, 0) < 0 || dup2(input[0], STDIN_FILENO) < 0 ||
                dup2(output[1], STDOUT_FILENO) < 0 || dup2(error[1], STDERR_FILENO) < 0) exec_error(ready[1]);
        }
        const int setup = 0;
        if (write(ready[1], &setup, sizeof(setup)) != sizeof(setup)) exec_error(ready[1]);
        execvp(command[0], command);
        exec_error(ready[1]);
    }
    close(ready[1]);
    if (session->pty) {
        session->input = session->output = master;
    } else {
        close(input[0]);
        close(output[1]);
        close(error[1]);
        session->input = input[1];
        session->output = output[0];
        session->error = error[0];
    }
    int failure = exec_status(ready[0]);
    close(ready[0]);
    if (failure) return failure;
    if (fd_flags(session->input) < 0 || fd_flags(session->output) < 0 ||
        (session->error >= 0 && fd_flags(session->error) < 0)) return errno;
    return 0;
}

static void signal_child(const struct session *session, int sig) {
    pid_t foreground = session->pty && session->output >= 0 ? tcgetpgrp(session->output) : -1;
    if (foreground > 0 && foreground != session->child) kill(-foreground, sig);
    if (kill(-session->child, sig) < 0) kill(session->child, sig);
}

static bool child_exited(pid_t child) {
    siginfo_t info = {0};
    return waitid(P_PID, (id_t)child, &info, WEXITED | WNOWAIT | WNOHANG) == 0 && info.si_pid == child;
}

static void close_input(struct session *session) {
    session->eof = true;
    if (!session->pty) close_fd(&session->input);
}

static bool read_output(int *fd, unsigned char stream, int64_t deadline) {
    unsigned char data[OUTPUT_CHUNK];
    ssize_t count = read(*fd, data, sizeof(data));
    if (count > 0) return event(stream, data, (size_t)count, deadline);
    if (count == 0 || (errno != EINTR && errno != EAGAIN && errno != EWOULDBLOCK)) close_fd(fd);
    return true;
}

static bool echo_state(struct session *session, struct termios *settings) {
    int state = 0;
    if (session->pty && session->output >= 0 && tcgetattr(session->output, settings) == 0) {
        state = !(settings->c_lflag & ICANON) ? 1 : (settings->c_lflag & ECHO) ? 2 : 3;
    }
    if (state != session->echo_state) {
        session->echo_state = state;
        unsigned char value = (unsigned char)state;
        return event(ECHO_STATE, &value, 1, millis() + 250);
    }
    return true;
}

static bool control(struct session *session, unsigned char type, const unsigned char *data, uint32_t size) {
    unsigned char result = APPLIED;
    struct termios settings = {0};
    if ((type == WRITE || type == EOF_INPUT) && !echo_state(session, &settings)) return false;
    if (type == CLOSE && size == 0) return false;
    if (type == WRITE && size > 0) {
        if (session->eof || session->input < 0 || (session->pty && session->output < 0)) result = INPUT_CLOSED;
        else if (session->echo_state == 3) result = AUTH_PAUSED;
        else if (!transfer(session->input, data, size, millis() + 250)) result = IO_FAILED;
    } else if (type == RESIZE && size == 4 && data[0] + data[1] > 0 && data[2] + data[3] > 0) {
        struct winsize size_request = {.ws_col = (unsigned short)(data[0] << 8 | data[1]),
            .ws_row = (unsigned short)(data[2] << 8 | data[3])};
        if (!session->pty) result = UNSUPPORTED;
        else if (session->output < 0 || ioctl(session->output, TIOCSWINSZ, &size_request) < 0) result = IO_FAILED;
    } else if (type == SIGNAL && size == 1 && data[0] < 4) {
        const int signals[] = {SIGINT, SIGTERM, SIGHUP, SIGKILL};
        signal_child(session, signals[data[0]]);
    } else if (type == EOF_INPUT && size == 0) {
        if (!session->eof && session->pty) {
            if (session->echo_state == 3) result = AUTH_PAUSED;
            else if (session->echo_state != 2 || settings.c_cc[VEOF] == _POSIX_VDISABLE) result = UNSUPPORTED;
            else if (!transfer(session->input, &settings.c_cc[VEOF], 1, millis() + 250)) result = IO_FAILED;
        }
        if (result == APPLIED) close_input(session);
    } else {
        return false;
    }
    return event(ACK, &result, 1, millis() + 250);
}

static bool supervise(struct session *session) {
    unsigned char frame[FRAME_HEADER + MAX_INPUT];
    size_t have = 0;
    size_t need = FRAME_HEADER;
    while (!stopping && !child_exited(session->child)) {
        struct pollfd fds[] = {{STDIN_FILENO, POLLIN, 0}, {session->output, POLLIN, 0}, {session->error, POLLIN, 0}};
        if (poll(fds, 3, 25) < 0 && errno != EINTR) return false;
        if (stopping || owner_lost(fds[0].revents)) return false;
        struct termios settings;
        if (!echo_state(session, &settings)) return false;
        if (fds[0].revents & POLLIN) {
            ssize_t count = read(STDIN_FILENO, frame + have, need - have);
            if (count == 0 || (count < 0 && errno != EINTR && errno != EAGAIN && errno != EWOULDBLOCK)) return false;
            if (count > 0) have += (size_t)count;
            if (have == FRAME_HEADER && need == FRAME_HEADER) {
                uint32_t length = read_u32(frame + 1);
                if (length > MAX_INPUT) return false;
                need += length;
            }
            if (have == need) {
                if (!control(session, frame[0], frame + FRAME_HEADER, (uint32_t)(need - FRAME_HEADER))) return false;
                have = 0;
                need = FRAME_HEADER;
            }
        }
        if (fds[1].revents && !read_output(&session->output, session->pty ? PTY : STDOUT, millis() + 250)) return false;
        if (fds[2].revents && !read_output(&session->error, STDERR, millis() + 250)) return false;
    }
    return !stopping;
}

static int reap(struct session *session, bool exited) {
    close_input(session);
    if (!exited) {
        signal_child(session, SIGHUP);
        signal_child(session, SIGTERM);
        int64_t deadline = millis() + 250;
        while (!child_exited(session->child) && millis() < deadline) poll(NULL, 0, 10);
    }
    signal_child(session, SIGKILL);
    int status = -1;
    pid_t result;
    do { result = waitpid(session->child, &status, 0); } while (result < 0 && errno == EINTR);
    return result == session->child ? status : -1;
}

static void drain(struct session *session) {
    const int64_t deadline = millis() + 100;
    for (int count = 0; count < 64 && millis() < deadline; count++) {
        struct pollfd fds[] = {{session->output, POLLIN, 0}, {session->error, POLLIN, 0}};
        if (poll(fds, 2, 0) <= 0) break;
        if (fds[0].revents && !read_output(&session->output, session->pty ? PTY : STDOUT, deadline)) break;
        if (fds[1].revents && !read_output(&session->error, STDERR, deadline)) break;
    }
}

int main(int argc, char **argv) {
    if (argc < 6 || strcmp(argv[1], "1") != 0) return 2;
    struct session session = {.child = -1, .input = -1, .output = -1, .error = -1, .echo_state = -1};
    session.pty = strcmp(argv[2], "pty") == 0;
    if (!session.pty && strcmp(argv[2], "pipe") != 0) return 2;
    long cols = strtol(argv[3], NULL, 10), rows = strtol(argv[4], NULL, 10);
    if (cols <= 0 || cols > UINT16_MAX || rows <= 0 || rows > UINT16_MAX) return 2;
    struct winsize size = {.ws_col = (unsigned short)cols, .ws_row = (unsigned short)rows};
    if (fd_flags(STDIN_FILENO) < 0 || fd_flags(STDOUT_FILENO) < 0 || fcntl(STDERR_FILENO, F_SETFD, FD_CLOEXEC) < 0) return 2;
    signal(SIGTERM, stop);
    signal(SIGHUP, stop);
    signal(SIGPIPE, SIG_IGN);
    int failure = launch(&session, argv + 5, &size);
    if (failure) {
        if (session.child > 0) reap(&session, false);
        unsigned char reason = (unsigned char)(failure < 0 ? EIO : failure);
        event(EXEC_FAILED, &reason, 1, millis() + 250);
        return 0;
    }
    bool started = event(OPENED, NULL, 0, millis() + 250);
    bool exited = started && supervise(&session);
    int status = reap(&session, exited);
    if (started) {
        drain(&session);
        unsigned char outcome[] = {status < 0 ? 0 : WIFEXITED(status) ? 1 : 2,
            status < 0 ? 0 : WIFEXITED(status) ? WEXITSTATUS(status) : WTERMSIG(status)};
        event(EXITED, outcome, sizeof(outcome), millis() + 250);
    }
    return 0;
}
