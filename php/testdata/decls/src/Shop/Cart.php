<?php

namespace Shop;

use Shop\Contracts\Priced;
use function Shop\Util\money;
use const Shop\Util\CURRENCY;

/**
 * A cart of goods.
 */
#[\Attribute]
final class Cart implements Priced, \Countable
{
    /** The tax rate. */
    public const TAX = 20, ROUND = 2;

    /**
     * Adds a good.
     */
    public function add(string $sku): void
    {
    }

    public function price(): int
    {
        return strlen(PHP_EOL) + money(1) + CURRENCY + LOCAL;
    }

    public function count(): int
    {
        return 0;
    }
}
