<?php

namespace Shop;

enum Suit: string
{
    case H = 'h';

    public function color(): string
    {
        return 'red';
    }
}
