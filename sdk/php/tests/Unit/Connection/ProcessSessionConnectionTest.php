<?php

namespace Dagger\Tests\Unit\Connection;

use PHPUnit\Framework\Attributes\Group;
use PHPUnit\Framework\Attributes\Test;
use PHPUnit\Framework\TestCase;
use Symfony\Component\Process\Exception\ProcessTimedOutException;
use Symfony\Component\Process\Process;

#[Group('unit')]
final class ProcessSessionConnectionTest extends TestCase
{
    private string $tempDir;

    protected function setUp(): void
    {
        if ('\\' === DIRECTORY_SEPARATOR) {
            self::markTestSkipped('The fake session is a POSIX shell script');
        }

        $this->tempDir = sys_get_temp_dir() . DIRECTORY_SEPARATOR . 'dagger-php-test-' . bin2hex(random_bytes(8));
        mkdir($this->tempDir);
    }

    protected function tearDown(): void
    {
        if (!isset($this->tempDir)) {
            return;
        }
        foreach (glob($this->tempDir . DIRECTORY_SEPARATOR . '*') ?: [] as $file) {
            unlink($file);
        }
        rmdir($this->tempDir);
    }

    #[Test]
    public function itReadsConnectParamsPrintedBeforeItStartsWaitingForThem(): void
    {
        // Like `dagger session`, the fake session prints its connect params as
        // soon as it is connected and then stays up. It marks when the params
        // are in its stdout pipe.
        $printed = $this->tempDir . DIRECTORY_SEPARATOR . 'printed';
        $cli = $this->tempDir . DIRECTORY_SEPARATOR . 'dagger';
        file_put_contents($cli, implode("\n", [
            '#!/bin/sh',
            'echo \'{"port":4242,"session_token":"test-token"}\'',
            "touch '{$printed}'",
            'exec sleep 3',
            '',
        ]));
        chmod($cli, 0700);

        // The connection runs in a PHP process of its own, so that one which
        // never sees the params fails this test instead of hanging it.
        $runner = new Process([
            PHP_BINARY,
            __DIR__ . DIRECTORY_SEPARATOR . 'connect-to-early-session.php',
            $cli,
            $printed,
            $this->tempDir,
        ]);
        $runner->setTimeout(30);
        try {
            $runner->run();
        } catch (ProcessTimedOutException) {
            self::fail('The connection waited 30 s for connect params the session had already printed');
        }

        self::assertSame(0, $runner->getExitCode(), $runner->getErrorOutput());
        self::assertSame("connected\n", $runner->getOutput());
    }
}
