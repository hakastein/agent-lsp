<?php

namespace Shop\Util;

/** Formats money. */
function money(int $cents): string
{
    return (string) $cents;
}

/** Not the doc of CURRENCY: a blank line follows. */

const CURRENCY = 'EUR';

define('LOCAL', 1);

trait Loggable
{
    public function log(): void
    {
    }
}

enum Suit: string
{
    case Hearts = 'h';
}
