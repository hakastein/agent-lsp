<?php

namespace Shop\Contracts;

interface Priced
{
    public function price(): int;

    public function discount(
        int $percent
    ): int;
}
