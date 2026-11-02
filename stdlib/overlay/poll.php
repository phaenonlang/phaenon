<?php

/**
 * Phaenon overlay: the polling API of PHP 8.6 (RFC poll_api). The events a
 * watcher waits for are a non-empty array of Io\Poll\Event cases, keys
 * ignored; anything else is a TypeError ("must be array of Event enums").
 */

namespace Io\Poll {
    final class Watcher
    {
        /**
         * @param non-empty-array<Event> $events
         */
        public function modify(array $events, mixed $data = null): void {}

        /**
         * @param non-empty-array<Event> $events
         */
        public function modifyEvents(array $events): void {}
    }

    final class Context
    {
        /**
         * @param non-empty-array<Event> $events
         */
        public function add(Handle $handle, array $events, mixed $data = null): Watcher {}
    }
}
