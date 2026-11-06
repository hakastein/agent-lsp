<?php

function sneaky(): void
{
    eval('global $e; $e = new \Shop\Wishlist();');
}
