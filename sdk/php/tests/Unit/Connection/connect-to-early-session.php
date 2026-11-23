<?php

// Used by ProcessSessionConnectionTest: connects to the fake session given as
// the first argument and prints "connected" once connect() returns.
//
// The connection logs that the session is starting once the session's process
// has started, before it reads the process's output. Holding that log until
// the session has printed its connect params (the file given as the second
// argument exists) makes the session faster than the connection, as a session
// that connects at once is when this process is slow to be scheduled, or its
// logger slow to write.

require dirname(__DIR__, 3) . '/vendor/autoload.php';

use Dagger\Connection\CliDownloader;
use Dagger\Connection\ProcessSessionConnection;
use Psr\Log\AbstractLogger;

[, $cli, $printed, $workDir] = $argv;
putenv("_EXPERIMENTAL_DAGGER_CLI_BIN={$cli}");

$logger = new class ($printed) extends AbstractLogger {
    public function __construct(private readonly string $printed)
    {
    }

    public function log($level, Stringable|string $message, array $context = []): void
    {
        if ('Starting Dagger session' !== (string) $message) {
            return;
        }
        for ($i = 0; $i < 500 && !file_exists($this->printed); $i++) {
            usleep(10_000);
        }
        if (!file_exists($this->printed)) {
            fwrite(STDERR, "The fake session did not print its connect params within 5 s\n");
            exit(2);
        }
    }
};

$connection = new ProcessSessionConnection($workDir, false, new CliDownloader());
$connection->setLogger($logger);
$connection->connect();
$connection->close();

echo "connected\n";
