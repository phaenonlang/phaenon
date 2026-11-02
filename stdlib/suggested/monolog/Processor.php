<?php

namespace Monolog\Processor;

use Monolog\LogRecord;

/**
 * Phaenon's declaration of Monolog 3's processor interface (monolog/monolog
 * 3.x).
 */
interface ProcessorInterface
{
    /**
     * @return LogRecord The processed record
     */
    public function __invoke(LogRecord $record);
}
